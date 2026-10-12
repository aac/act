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
