package main

// act-94272e: a write whose commit failed must not read back as having
// happened. These tests drive the real binary as a subprocess — the same
// boundary the bug was found at — because that is the only place the two
// disagreeing surfaces (the command's exit code, and a LATER `act show` /
// `act list` invocation folding the op log from disk) are both visible.
// An in-process test cannot reproduce it: the fold happens on the next
// process's read.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// breakCommits makes every commit in the nested .act/ repo fail, by
// pointing gpg signing at a program that does not exist. This is the
// ticket's own repro vector, and it is the honest one: it fails inside
// `git commit` AFTER staging succeeded, which is precisely the window
// where an op file exists on disk but no commit references it.
func breakCommits(t *testing.T, dir string) {
	t.Helper()
	actDir := filepath.Join(dir, ".act")
	mustGitConfig(t, actDir, "commit.gpgsign", "true")
	mustGitConfig(t, actDir, "gpg.program", "/nonexistent/gpg")
}

func unbreakCommits(t *testing.T, dir string) {
	t.Helper()
	actDir := filepath.Join(dir, ".act")
	for _, key := range []string{"commit.gpgsign", "gpg.program"} {
		// --unset on an absent key exits 5; ignore, the goal is absence.
		_ = exec.Command("git", "-C", actDir, "config", "--unset", key).Run()
	}
}

func mustGitConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "config", key, value).CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v: %s", key, err, out)
	}
}

// newFailVisibilityRepo bootstraps a host git repo with act initialized
// and returns its path.
func newFailVisibilityRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, stderr, code := runActIn(t, dir, "init", "--json"); code != 0 {
		t.Fatalf("act init: exit %d; stderr=%s", code, stderr)
	}
	return dir
}

func createIssueFV(t *testing.T, dir, title string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "create", title, "--json")
	if code != 0 {
		t.Fatalf("act create: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("parse create output %q: %v", out, err)
	}
	if res.ID == "" {
		t.Fatalf("empty id in create output: %s", out)
	}
	return res.ID
}

func showStatus(t *testing.T, dir, id string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("parse show output %q: %v", out, err)
	}
	return res.Status
}

// TestDocClaim_FailedCommitStaysUnclosed is the asserting test for the
// `act help errors` claim "AN OP WHOSE COMMIT FAILED IS INVISIBLE".
//
// Contract under test (act-94272e's acceptance criterion): `act show` /
// `act list` and the close command's exit status agree about whether the
// issue is closed after a commit failure.
//
// Before the fix: close exited 1 with commit_failed, and the very next
// `act show` reported status closed, because the fold read the op file
// the failed close had left in .act/ops.
func TestDocClaim_FailedCommitStaysUnclosed(t *testing.T) {
	dir := newFailVisibilityRepo(t)
	id := createIssueFV(t, dir, "close-visibility probe")

	if got := showStatus(t, dir, id); got != "open" {
		t.Fatalf("precondition: status = %q; want open", got)
	}

	breakCommits(t, dir)
	out, _, code := runActIn(t, dir, "close", id, "--json")
	if code == 0 {
		t.Fatalf("act close: exit 0 with commits broken; stdout=%s", out)
	}

	var env struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("parse close error envelope %q: %v", out, err)
	}
	if env.Error != "commit_failed" {
		t.Fatalf("close error = %q; want commit_failed (stdout=%s)", env.Error, out)
	}

	// THE CONTRACT: a separate process reading the store must agree with
	// the exit code above.
	if got := showStatus(t, dir, id); got != "open" {
		t.Errorf("act show status = %q after a failed close (exit %d); want open — "+
			"the command reported failure, so the store must not report the close as landed", got, code)
	}

	listOut, _, listCode := runActIn(t, dir, "list", "--json")
	if listCode != 0 {
		t.Fatalf("act list: exit %d; stdout=%s", listCode, listOut)
	}
	var listRes struct {
		Issues []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(listOut)), &listRes); err != nil {
		t.Fatalf("parse list output %q: %v", listOut, err)
	}
	found := false
	for _, it := range listRes.Issues {
		if it.ID == id {
			found = true
			if it.Status != "open" {
				t.Errorf("act list status = %q after a failed close; want open", it.Status)
			}
		}
	}
	if !found {
		t.Errorf("issue %s missing from act list after a failed close; the failed close must not "+
			"remove it either", id)
	}

	// No close op may remain in the op log — that file IS the disagreement.
	matches, _ := filepath.Glob(filepath.Join(dir, ".act", "ops", id, "*", "*-close.json"))
	if len(matches) != 0 {
		t.Errorf("close op file(s) remain in .act/ops after a failed commit: %v", matches)
	}
}

// TestDocClaim_FailedCommitPreservesEnvelope asserts the second half of
// the same help claim: the envelope is preserved, not deleted, at the
// path reported in details.quarantined_op — and restoring it replays the
// write verbatim, which is the original "retry without rebuilding the
// envelope" intent the old leave-it-in-place behavior was protecting.
func TestDocClaim_FailedCommitPreservesEnvelope(t *testing.T) {
	dir := newFailVisibilityRepo(t)
	id := createIssueFV(t, dir, "envelope-preservation probe")

	breakCommits(t, dir)
	out, _, code := runActIn(t, dir, "close", id, "--json")
	if code == 0 {
		t.Fatalf("act close: exit 0 with commits broken; stdout=%s", out)
	}
	var env struct {
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("parse close error envelope %q: %v", out, err)
	}
	q, _ := env.Details["quarantined_op"].(string)
	if q == "" {
		t.Fatalf("close error envelope has no details.quarantined_op: %s", out)
	}
	body, err := os.ReadFile(q)
	if err != nil {
		t.Fatalf("quarantined op file %s unreadable: %v", q, err)
	}
	if !strings.Contains(string(body), `"op_type":"close"`) {
		t.Errorf("quarantined file does not look like the close envelope: %s", body)
	}
	if !strings.HasPrefix(q, filepath.Join(dir, ".act", ".failed-ops")) {
		t.Errorf("quarantine path %q is not under .act/.failed-ops", q)
	}

	// Restoring the preserved envelope under .act/ops replays the close —
	// no rebuild required.
	rel, err := filepath.Rel(filepath.Join(dir, ".act"), q)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	// rel is ".failed-ops/<stamp>/ops/<id>/<shard>/<file>"; drop the first
	// two segments to recover the original ops/-relative path.
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		t.Fatalf("unexpected quarantine layout: %s", rel)
	}
	restored := filepath.Join(append([]string{dir, ".act"}, parts[2:]...)...)
	if err := os.MkdirAll(filepath.Dir(restored), 0o755); err != nil {
		t.Fatalf("mkdir restore: %v", err)
	}
	if err := os.WriteFile(restored, body, 0o644); err != nil {
		t.Fatalf("restore op file: %v", err)
	}
	unbreakCommits(t, dir)
	if got := showStatus(t, dir, id); got != "closed" {
		t.Errorf("status after restoring the preserved envelope = %q; want closed — "+
			"the envelope must be replayable without being rebuilt", got)
	}
}

// TestDocClaim_FailedCommitCreateStaysInvisible covers the same contract
// on the shared single-op write path (`act create`), not just close. The
// ticket was filed against close, but the defect lived in the write
// helper every non-close write op goes through, so the fix is only real
// if it holds there too.
func TestDocClaim_FailedCommitCreateStaysInvisible(t *testing.T) {
	dir := newFailVisibilityRepo(t)
	breakCommits(t, dir)

	out, _, code := runActIn(t, dir, "create", "must not appear", "--json")
	if code == 0 {
		t.Fatalf("act create: exit 0 with commits broken; stdout=%s", out)
	}

	listOut, _, listCode := runActIn(t, dir, "list", "--json")
	if listCode != 0 {
		t.Fatalf("act list: exit %d; stdout=%s", listCode, listOut)
	}
	if strings.Contains(listOut, "must not appear") {
		t.Errorf("act create exited %d but the issue is in act list: %s", code, listOut)
	}
}
