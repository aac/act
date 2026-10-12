package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// idxBreakIndex replaces .act/index.db with a directory, so nothing can open
// it as a SQLite file, and returns a func that removes the directory again
// (the "repair": the next command rebuilds the index from the op log).
func idxBreakIndex(t *testing.T, dir string) (repair func()) {
	t.Helper()
	db := filepath.Join(dir, ".act", "index.db")
	matches, _ := filepath.Glob(db + "*")
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			t.Fatalf("remove %s: %v", m, err)
		}
	}
	if err := os.Mkdir(db, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", db, err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(db); err != nil {
			t.Fatalf("repair: remove %s: %v", db, err)
		}
	}
}

// idxCreateOps returns every create op file in the tracker.
func idxCreateOps(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	_ = filepath.Walk(filepath.Join(dir, ".act", "ops"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, "-create.json") {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// TestDocClaim_CreateIndexUnopenable_ReportsTheCreatedIssue pins act-e3782c:
// when `act create` has written and committed its op and only the index
// refresh fails, the caller is told the issue exists. A caller that reads
// exit status alone (`out=$(act create --json …) || stop`) must not be led to
// retry and file a duplicate.
func TestDocClaim_CreateIndexUnopenable_ReportsTheCreatedIssue(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "idx seed")
	repair := idxBreakIndex(t, dir)

	out, stderr, code := runActIn(t, dir, "create", "--json", "idx after index broke")

	ops := idxCreateOps(t, dir)
	if len(ops) != 2 {
		t.Fatalf("premise: want the create op written despite the broken index (2 create ops), got %d: %v\nexit=%d stdout=%s stderr=%s",
			len(ops), ops, code, out, stderr)
	}
	if code != 0 {
		t.Fatalf("create with an unopenable index.db: exit %d, want 0 (the op is committed; the index is a cache)\nstdout=%s\nstderr=%s",
			code, out, stderr)
	}
	var res struct {
		Ok        bool     `json:"ok"`
		ID        string   `json:"id"`
		Committed bool     `json:"committed"`
		Warnings  []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("parse create output %q: %v", out, err)
	}
	if !res.Ok || res.ID == "" || !res.Committed {
		t.Fatalf("create result: want ok, an id and committed:true, got %s", out)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "index_update_failed" {
		t.Errorf("warnings = %v, want [index_update_failed]\nstdout=%s", res.Warnings, out)
	}

	// The op is committed in the nested tracker repo, not just on disk.
	st, err := exec.Command("git", "-C", filepath.Join(dir, ".act"), "status", "--porcelain", "--", "ops").CombinedOutput()
	if err != nil || strings.TrimSpace(string(st)) != "" {
		t.Errorf("ops left uncommitted after create (err=%v): %s", err, st)
	}

	repair()
	showOut, showErr, showCode := runActIn(t, dir, "show", "--json", res.ID)
	if showCode != 0 || !strings.Contains(showOut, "idx after index broke") {
		t.Errorf("show %s after repairing the index: exit %d\nstdout=%s\nstderr=%s", res.ID, showCode, showOut, showErr)
	}
	listOut, _, listCode := runActIn(t, dir, "list", "--json")
	if listCode != 0 || !strings.Contains(listOut, res.ID) {
		t.Errorf("list after repairing the index does not show %s: exit %d\n%s", res.ID, listCode, listOut)
	}
}

// TestDocClaim_CreateIndexUnopenable_HumanWarnsAndReadsStillFail pins the
// rest of the spec paragraph: without --json the warning is a stderr line
// beside the usual Created line, and the broken index is not hidden — the
// next read fails in its own name, as list/ready pin elsewhere.
func TestDocClaim_CreateIndexUnopenable_HumanWarnsAndReadsStillFail(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "idx seed")
	idxBreakIndex(t, dir)

	out, stderr, code := runActIn(t, dir, "create", "idx human after index broke")
	if code != 0 {
		t.Fatalf("create: exit %d, want 0\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	if !strings.HasPrefix(out, "Created act-") {
		t.Errorf("stdout = %q, want the usual Created line", out)
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "index.db could not be updated") || !strings.Contains(stderr, "do not retry") {
		t.Errorf("stderr = %q, want a warning that the index could not be updated and not to retry", stderr)
	}

	// The read commands keep their own pinned failure: exit 1, named.
	pass5Run(t, dir, 1, "index_open_failed", "list", "--json")
	pass5Run(t, dir, 1, "index_open_failed", "ready", "--json")
}

// TestDocClaim_CreateIndexUnopenable_BlocksEdgeStillLands covers the second
// index refresh in create's tail (the --blocks targets): the batch is
// committed, so that refresh failing is the same warning, once.
func TestDocClaim_CreateIndexUnopenable_BlocksEdgeStillLands(t *testing.T) {
	dir := sp6Site(t)
	target := createIssue(t, dir, "idx blocked target")
	repair := idxBreakIndex(t, dir)

	out, stderr, code := runActIn(t, dir, "create", "--json", "--blocks", target, "idx blocker")
	if code != 0 {
		t.Fatalf("create --blocks: exit %d, want 0\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	var res struct {
		ID       string   `json:"id"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || res.ID == "" {
		t.Fatalf("parse create output %q: %v", out, err)
	}
	if strings.Join(res.Warnings, ",") != "index_update_failed" {
		t.Errorf("warnings = %v, want exactly [index_update_failed]", res.Warnings)
	}

	repair()
	showOut, _, showCode := runActIn(t, dir, "show", "--json", target)
	if showCode != 0 || !strings.Contains(showOut, res.ID) {
		t.Errorf("show %s after repair does not name its blocker %s: exit %d\n%s", target, res.ID, showCode, showOut)
	}
}
