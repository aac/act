package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ixwFreezeIndex makes .act/index.db read-only after bringing it up to date,
// and returns a func that makes it writable again.
//
// This is the honest way to fail only the index refresh that follows a
// committed op (act-c3aa0b). close, reopen, dep add and update --claim read
// the index before they write, so an index that cannot be opened at all
// stops them before any op exists (index_open_failed — a safe retry). A
// read-only index answers those reads and refuses only the write.
func ixwFreezeIndex(t *testing.T, dir string) (thaw func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions; a read-only index.db stays writable")
	}
	if out, stderr, code := runActIn(t, dir, "list", "--json", "--all"); code != 0 {
		t.Fatalf("list before freezing the index: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".act", "index.db*"))
	if len(files) == 0 {
		t.Fatalf("no .act/index.db to freeze in %s", dir)
	}
	for _, f := range files {
		if err := os.Chmod(f, 0o444); err != nil {
			t.Fatalf("chmod %s: %v", f, err)
		}
	}
	return func() {
		t.Helper()
		for _, f := range files {
			if err := os.Chmod(f, 0o644); err != nil {
				t.Fatalf("thaw: chmod %s: %v", f, err)
			}
		}
	}
}

// ixwResult is the union of the keys the write commands' results carry.
type ixwResult struct {
	Error    string   `json:"error"`
	Message  string   `json:"message"`
	Warnings []string `json:"warnings"`
}

// ixwRunFrozen runs one write command against a frozen index and asserts
// the act-c3aa0b contract: the op is committed, the exit is 0, and the
// result names index_update_failed as a warning, once. It returns stdout.
func ixwRunFrozen(t *testing.T, dir string, args ...string) string {
	t.Helper()
	before := ixwOpCommits(t, dir)
	out, stderr, code := runActIn(t, dir, args...)
	after := ixwOpCommits(t, dir)
	if after <= before {
		t.Fatalf("premise: act %s wrote no op commit against the frozen index (%d before, %d after); the failure under test is the refresh AFTER a commit\nexit=%d stdout=%s stderr=%s",
			strings.Join(args, " "), before, after, code, out, stderr)
	}
	st, err := exec.Command("git", "-C", filepath.Join(dir, ".act"), "status", "--porcelain", "--", "ops").CombinedOutput()
	if err != nil || strings.TrimSpace(string(st)) != "" {
		t.Errorf("ops left uncommitted after act %s (err=%v): %s", strings.Join(args, " "), err, st)
	}
	if code != 0 {
		t.Fatalf("act %s with its op committed and only the index refresh failing: exit %d, want 0\nstdout=%s\nstderr=%s",
			strings.Join(args, " "), code, out, stderr)
	}
	var res ixwResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("parse act %s output %q: %v", strings.Join(args, " "), out, err)
	}
	if res.Error != "" {
		t.Errorf("act %s result carries error %q; want the command's usual result", strings.Join(args, " "), res.Error)
	}
	if strings.Join(res.Warnings, ",") != "index_update_failed" {
		t.Errorf("act %s warnings = %v, want exactly [index_update_failed]\nstdout=%s", strings.Join(args, " "), res.Warnings, out)
	}
	return out
}

// ixwOpCommits counts the commits in the nested tracker repo.
func ixwOpCommits(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(dir, ".act"), "rev-list", "--count", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("count tracker commits: %v: %s", err, out)
	}
	n := 0
	for _, c := range strings.TrimSpace(string(out)) {
		n = n*10 + int(c-'0')
	}
	return n
}

// ixwShow returns `act show --json <id>` parsed, after the index is writable
// again: the next read rebuilds the cache from the op log.
func ixwShow(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", "--json", id)
	if code != 0 {
		t.Fatalf("show %s after thawing the index: exit %d\nstdout=%s\nstderr=%s", id, code, out, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("parse show %s output %q: %v", id, out, err)
	}
	return m
}

// TestDocClaim_IxwClose_IndexUpdateFailedIsAWarning: a close whose op is
// committed and whose index refresh then fails says the close happened.
func TestDocClaim_IxwClose_IndexUpdateFailedIsAWarning(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "ixw close")
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "close", "--json", "--no-code", "--reason", "ixw", id)
	if !strings.Contains(out, `"id":"`+id+`"`) || !strings.Contains(out, `"committed":true`) {
		t.Errorf("close result = %s; want the usual keys with id %s and committed:true", out, id)
	}

	thaw()
	if got := ixwShow(t, dir, id)["status"]; got != "closed" {
		t.Errorf("status after thaw = %v, want closed", got)
	}
}

// TestDocClaim_IxwUpdate_IndexUpdateFailedIsAWarning: the same for a field
// update, and for one whose refresh fails because the fold rejects an op.
func TestDocClaim_IxwUpdate_IndexUpdateFailedIsAWarning(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "ixw update")
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "update", "--json", "--priority", "1", id)
	if !strings.Contains(out, `"ops_written":1`) || !strings.Contains(out, `"committed":true`) {
		t.Errorf("update result = %s; want ops_written:1 and committed:true", out)
	}

	thaw()
	if got := ixwShow(t, dir, id)["priority"]; got != float64(1) {
		t.Errorf("priority after thaw = %v, want 1", got)
	}

	// The refresh also fails when the fold rejects one of the issue's ops.
	// The update op is committed all the same.
	dir2 := sp6Site(t)
	id2 := createIssue(t, dir2, "ixw update unfoldable")
	sp8BadOpVersion(t, dir2, id2)
	ixwRunFrozen(t, dir2, "update", "--json", "--priority", "1", id2)
}

// TestDocClaim_IxwUpdateClaim_IndexUpdateFailedIsAWarning: a won claim.
func TestDocClaim_IxwUpdateClaim_IndexUpdateFailedIsAWarning(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "ixw claim")
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "update", "--json", "--claim", id)
	if !strings.Contains(out, `"claimed":true`) {
		t.Errorf("claim result = %s; want claimed:true", out)
	}

	thaw()
	if got := ixwShow(t, dir, id)["status"]; got != "in_progress" {
		t.Errorf("status after thaw = %v, want in_progress", got)
	}
}

// TestDocClaim_IxwReopen_IndexUpdateFailedIsAWarning: a reopen.
func TestDocClaim_IxwReopen_IndexUpdateFailedIsAWarning(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "ixw reopen")
	pass5Run(t, dir, 0, "", "close", "--json", "--no-code", id)
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "reopen", "--json", id)
	if !strings.Contains(out, `"id":"`+id+`"`) || !strings.Contains(out, `"committed":true`) {
		t.Errorf("reopen result = %s; want id %s and committed:true", out, id)
	}

	thaw()
	if got := ixwShow(t, dir, id)["status"]; got != "open" {
		t.Errorf("status after thaw = %v, want open", got)
	}
}

// TestDocClaim_IxwDelete_IndexUpdateFailedIsAWarning: a cascading delete
// refreshes one row per tombstone; the warning appears once.
func TestDocClaim_IxwDelete_IndexUpdateFailedIsAWarning(t *testing.T) {
	dir := sp6Site(t)
	parent := createIssue(t, dir, "ixw delete parent")
	child := createIssue(t, dir, "ixw delete child", "--parent", parent)
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "delete", "--json", "--cascade", "--reason", "ixw", parent)
	if !strings.Contains(out, `"ops_written":2`) || !strings.Contains(out, child) {
		t.Errorf("delete result = %s; want ops_written:2 and %s among tombstoned", out, child)
	}

	thaw()
	listOut, _ := pass5Run(t, dir, 0, "", "list", "--json", "--all")
	if strings.Contains(listOut, parent) || strings.Contains(listOut, child) {
		t.Errorf("list after thaw still shows a deleted issue (%s, %s)\n%s", parent, child, listOut)
	}
}

// TestDocClaim_IxwDepAdd_IndexUpdateFailedIsAWarning: a dep edge.
func TestDocClaim_IxwDepAdd_IndexUpdateFailedIsAWarning(t *testing.T) {
	dir := sp6Site(t)
	child := createIssue(t, dir, "ixw dep child")
	parent := createIssue(t, dir, "ixw dep parent")
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "dep", "add", "--json", child, parent)
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"committed":true`) {
		t.Errorf("dep add result = %s; want ok:true and committed:true", out)
	}

	thaw()
	showOut, _ := pass5Run(t, dir, 0, "", "show", "--json", child)
	if !strings.Contains(showOut, parent) {
		t.Errorf("show %s after thaw does not name its blocker %s\n%s", child, parent, showOut)
	}
}

// TestDocClaim_IxwHumanWarnsOnStderrAndReadsStillFail pins the rest of the
// spec paragraph: without --json the warning is a stderr line beside the
// command's usual stdout line, and the index that could not be written is
// not hidden — the next read fails in its own name until it is writable.
func TestDocClaim_IxwHumanWarnsOnStderrAndReadsStillFail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		args   func(t *testing.T, dir string) []string
	}{
		{"close", "Closed act-", func(t *testing.T, dir string) []string {
			return []string{"close", "--no-code", createIssue(t, dir, "ixw h close")}
		}},
		{"update", "Updated act-", func(t *testing.T, dir string) []string {
			return []string{"update", "--priority", "1", createIssue(t, dir, "ixw h update")}
		}},
		{"update --claim", "Claimed act-", func(t *testing.T, dir string) []string {
			return []string{"update", "--claim", createIssue(t, dir, "ixw h claim")}
		}},
		{"reopen", "Reopened act-", func(t *testing.T, dir string) []string {
			id := createIssue(t, dir, "ixw h reopen")
			pass5Run(t, dir, 0, "", "close", "--json", "--no-code", id)
			return []string{"reopen", id}
		}},
		{"delete", "Deleted act-", func(t *testing.T, dir string) []string {
			return []string{"delete", "--reason", "ixw", createIssue(t, dir, "ixw h delete")}
		}},
		{"dep add", "act-", func(t *testing.T, dir string) []string {
			return []string{"dep", "add", createIssue(t, dir, "ixw h child"), createIssue(t, dir, "ixw h parent")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sp6Site(t)
			args := tc.args(t, dir)
			ixwFreezeIndex(t, dir)

			out, stderr, code := runActIn(t, dir, args...)
			if code != 0 {
				t.Fatalf("act %s: exit %d, want 0\nstdout=%s\nstderr=%s", strings.Join(args, " "), code, out, stderr)
			}
			if !strings.HasPrefix(out, tc.stdout) {
				t.Errorf("stdout = %q, want the usual line starting %q", out, tc.stdout)
			}
			want := "act " + tc.name + ": warning: "
			if tc.name == "update --claim" {
				want = "act update: warning: "
			}
			if !strings.Contains(stderr, want) || !strings.Contains(stderr, "index.db could not be updated") || !strings.Contains(stderr, "do not retry") {
				t.Errorf("stderr = %q, want a line starting %q that says the index could not be updated and not to retry", stderr, want)
			}

			// The index is now behind the op log and still cannot be
			// written, so a read fails in its own name rather than
			// answering from the stale cache.
			pass5Run(t, dir, 1, "index_rebuild_failed", "list", "--json")
		})
	}
}
